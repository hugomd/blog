package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
)

type queueCommit struct {
	Expected, Generation string
	Job                  *queueJob
	Remove               []string
	PublicationWrite     *publicationWrite
}

type queueStorage interface {
	Load(context.Context) (durableQueue, error)
	Commit(context.Context, queueCommit) error
}

type sqliteQueueStorage struct {
	db   *sql.DB
	lock *os.File
}

func openQueueStorage(filename string) (*sqliteQueueStorage, error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filename+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another Studio process owns this queue database")
	}
	s := &sqliteQueueStorage{lock: lock}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	query := url.Values{"_pragma": {"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)", "foreign_keys(ON)"}, "_txlock": {"immediate"}}
	dsn := (&url.URL{Scheme: "file", Path: filename, RawQuery: query.Encode()}).String()
	s.db, err = sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s.db.SetMaxOpenConns(1)
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version == 0 {
		_, err = s.db.Exec(`BEGIN IMMEDIATE;
			CREATE TABLE queue_meta (id INTEGER PRIMARY KEY CHECK(id=1), generation TEXT NOT NULL, publication_write BLOB);
			INSERT INTO queue_meta VALUES(1, '', NULL);
			CREATE TABLE queue_jobs (sequence INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, body BLOB NOT NULL);
			CREATE TABLE queue_targets (photo_id TEXT PRIMARY KEY, job_id TEXT NOT NULL REFERENCES queue_jobs(id) ON DELETE CASCADE);
			PRAGMA user_version=1;
			COMMIT;`)
		if err != nil {
			return nil, err
		}
	} else if version != 1 {
		return nil, errors.New("unsupported queue database version")
	}
	ok = true
	return s, nil
}

func (s *sqliteQueueStorage) Close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
	}
	if s.lock != nil {
		s.lock.Close()
		s.lock = nil
	}
	return err
}

func (s *sqliteQueueStorage) Load(ctx context.Context) (durableQueue, error) {
	q := durableQueue{Version: 1, Jobs: []queueJob{}}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return q, err
	}
	defer tx.Rollback()
	var intent []byte
	if err := tx.QueryRowContext(ctx, "SELECT generation, publication_write FROM queue_meta WHERE id=1").Scan(&q.Generation, &intent); err != nil {
		return q, err
	}
	if len(intent) > 0 {
		if err := json.Unmarshal(intent, &q.PublicationWrite); err != nil {
			return q, err
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT body FROM queue_jobs ORDER BY sequence")
	if err != nil {
		return q, err
	}
	defer rows.Close()
	for rows.Next() {
		var body []byte
		var job queueJob
		if err := rows.Scan(&body); err != nil {
			return q, err
		}
		if err := json.Unmarshal(body, &job); err != nil {
			return q, err
		}
		q.Jobs = append(q.Jobs, job)
	}
	if err := rows.Err(); err != nil {
		return q, err
	}
	return q, tx.Commit()
}

func (s *sqliteQueueStorage) Commit(ctx context.Context, commit queueCommit) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	intent, err := json.Marshal(commit.PublicationWrite)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE queue_meta SET generation=?, publication_write=? WHERE id=1 AND generation=?", commit.Generation, intent, commit.Expected)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return errConflict
	}
	for _, id := range commit.Remove {
		if _, err := tx.ExecContext(ctx, "DELETE FROM queue_jobs WHERE id=? AND json_extract(body, '$.state') IN ('succeeded','cancelled','rejected')", id); err != nil {
			return err
		}
	}
	if job := commit.Job; job != nil {
		body, err := json.Marshal(job)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO queue_jobs(id,body) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET body=excluded.body", job.ID, body); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM queue_targets WHERE job_id=?", job.ID); err != nil {
			return err
		}
		if queuePending(*job) {
			for _, target := range job.Targets {
				if _, err := tx.ExecContext(ctx, "INSERT INTO queue_targets(photo_id,job_id) VALUES(?,?)", target.ID, job.ID); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}
