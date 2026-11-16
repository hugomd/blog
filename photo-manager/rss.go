package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"strings"
	"time"
)

type rssFeed struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Atom    string   `xml:"xmlns:atom,attr"`
	Channel struct {
		Title         string `xml:"title"`
		Link          string `xml:"link"`
		Description   string `xml:"description"`
		Language      string `xml:"language"`
		LastBuildDate string `xml:"lastBuildDate,omitempty"`
		Self          struct {
			Href string `xml:"href,attr"`
			Rel  string `xml:"rel,attr"`
			Type string `xml:"type,attr"`
		} `xml:"atom:link"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate,omitempty"`
	Description string `xml:"description"`
}

// Only pass the committed public projection, never the working catalog or a
// protected release. Retrying an archive/delete must repair the feed even if
// the preceding attempt already committed the redacted JSON index.
func (a *app) writeRSS(ctx context.Context, live catalog) error {
	var feed rssFeed
	feed.Version, feed.Atom = "2.0", "http://www.w3.org/2005/Atom"
	channel := &feed.Channel
	channel.Title = "Photostream :: Hugo’s Blog"
	channel.Link = strings.TrimRight(a.config.GalleryURL, "/") + "/"
	channel.Description = "Photographs by Hugo, from travels and everyday life."
	channel.Language = "en-us"
	channel.Self.Href = strings.TrimRight(a.config.PublicURL, "/") + "/index.xml"
	channel.Self.Rel, channel.Self.Type = "self", "application/rss+xml"
	if date, err := time.Parse(time.RFC3339Nano, live.PublishedAt); err == nil {
		channel.LastBuildDate = date.UTC().Format(time.RFC1123Z)
	}
	for _, p := range live.Photos {
		if p.Kind != "" && p.Kind != "image" {
			continue
		}
		link := channel.Link + p.ID + "/"
		title := p.Alt
		if title == "" {
			title = p.CapturedDate
		}
		if title == "" {
			title = "Photograph"
		}
		src := strings.TrimRight(a.config.PublicURL, "/") + "/photos/" + p.ID + "/2560.jpg"
		description := fmt.Sprintf(`<a href="%s"><img src="%s" alt="%s"></a>`, html.EscapeString(link), html.EscapeString(src), html.EscapeString(p.Alt))
		if p.Caption != "" {
			description += "<p>" + html.EscapeString(p.Caption) + "</p>"
		}
		item := rssItem{Title: title, Link: link, GUID: link, Description: description}
		if date, err := time.Parse(time.RFC3339Nano, p.UploadedAt); err == nil {
			item.PubDate = date.UTC().Format(time.RFC1123Z)
		}
		channel.Items = append(channel.Items, item)
	}
	body, err := xml.Marshal(feed)
	if err != nil {
		return err
	}
	body = append([]byte(xml.Header), body...)
	previous, err := a.store.Read(ctx, "public", "index.xml")
	if err != nil {
		return err
	}
	etag := ""
	if previous != nil {
		if bytes.Equal(previous.Body, body) {
			return nil
		}
		etag = previous.ETag
	}
	return a.store.Write(ctx, "public", "index.xml", body, "application/rss+xml; charset=utf-8", "public, max-age=30, must-revalidate", &etag)
}
