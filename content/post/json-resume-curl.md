---
date: "2017-02-02T20:55:04+11:00"
title: "Responding to cURL with a JSON Resumé 📚"
tags: ["tutorial", "curl", "json"]
---

Over the past couple of months I've received numerous messages asking me how I've made this website respond to cURL requests with JSON (a little like [JSON Resumé](https://jsonresume.org)).  If you're out of the loop, you can either `curl hugo.md` or check out this GIF:

![](screen-capture.gif)

It's super simple to setup — I've done it with nginx and with Caddy. All it does is:  
1. Check the user agent on incoming requests to the root site (`example.site/`)  
2. If the user agent is cURL, respond with a JSON file

With [nginx](https://www.nginx.com):

{{< highlight text >}}
server {
  listen 80;
  server_name example.site;
  root /home/you/www;

  location / {
    if ($http_user_agent ~* ^curl) {
      rewrite ^/$ "/path/to/about.json";
      break;
    }
  }
}
{{< / highlight >}}

With [Caddy](https://caddyserver.com), it's even easier:

{{< highlight text >}}
example.site {
    root /home/you/www
    rewrite {
        if {>User-Agent} has curl
        if {path} is /
        to /path/to/about.json
    }
}
{{< / highlight >}}

Thought this was neat? [Tweet me!](https://twitter.com/hugojmd)

If you liked this post, you'll probably enjoy one of my other projects, [`parrot.live`](https://github.com/hugomd/parrot.live) 🐦 Try `curl parrot.live` to see it in action!
