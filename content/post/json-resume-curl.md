+++
date = "2017-02-02T20:55:04+11:00"
description = "$ curl hugo.md 💻"
title = "Responding to cURL with a JSON Resumé 📚"
tags=[ "tutorial", "curl", "json" ]
+++

Over the past couple of months I've received numerous messages asking me how I've made this website respond to cURL requests with JSON (a little like [JSON Resumé](https://jsonresume.org)).  If you're out of the loop, you can either `curl hugo.md` or check out this GIF:

![](screen-capture.gif)

It's super simple to setup — I've done it with nginx and with Caddy. All it does is:  
1. Check the user agent on incoming requests   
2. If the user agent is cURL, respond with a JSON file

With [nginx](https://www.nginx.com):

```nginx
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
```

With [Caddy](https://caddyserver.com), it's even easier:

```
example.site {
    root /home/you/www
    rewrite {
        if {>User-Agent} has curl
        if {path} is /
        to /path/to/about.json
    }
}
```

Thought this was neat? [Tweet me!](https://twitter.com/hugojmd)
