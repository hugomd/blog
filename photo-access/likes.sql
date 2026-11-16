-- Apply to the REACTIONS D1 database: wrangler d1 execute <database> --remote --file photo-access/likes.sql
CREATE TABLE IF NOT EXISTS photo_likes (
  photo_id TEXT PRIMARY KEY,
  count INTEGER NOT NULL
);
