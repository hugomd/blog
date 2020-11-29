---
title: "Adding Webmentions to my blog"
date: "2020-07-08T18:28:14+10:00"
description: "Fetch Webmentions from micro.blog and display them on your blog"
tags: ["indieweb"]
---

[Webmentions](https://indieweb.org/Webmention) is a relatively standard for federating comments and mentions across websites. When you mention someone or like a post, you own your data, but it can show up on their site — neat!

I use [micro.blog](https://micro.blog) to post short messages to the internet and push them to [my Twitter](https://twitter.com/hugojmd), [Mastodon](https://melb.social/@hugo), and otherwise interact with internet friends.

Micro.blog [supports webmentions out of the box](https://help.micro.blog/2017/webmention/), which means if you respond to someone's post there, they can show it on their blog, and it will show up in the micro.blog feed.

I'm in an odd situation because I don't host my blog with micro.blog directly, I use a static site generator called [Hugo](https://gohugo.io/), and because it's static I don't have an easy means of receiving webmentions myself.

This is where micro.blog comes in. Any post from this blog gets posted to micro.blog automatically, as it syncs via RSS, and any post on there can be responded to via webmentions. All I have to do, is redirect webmentions to this website over to micro.blog.

Implementing this involves adding some links to your `<head>` tags, and then fetching comments from micro.blog.

Add webmention callbacks to your blog's `<head>`, replacing `USERNAME` with your microblog username:
```html
<link href="https://micro.blog/USERNAME" rel="me" />
<link rel="webmention" href="https://micro.blog/webmention" />
<link rel="authorization_endpoint" href="https://micro.blog/indieauth/auth" />
<link rel="token_endpoint" href="https://micro.blog/indieauth/token" />
```

After that, you can add a section for fetching comments as I've done [here](https://github.com/hugomd/blog/blob/97143d2bf7d2f7142632e9b970cb94c50bb948e1/layouts/partials/comments.html#L54-L78).

Related posts:

* [Adding webmentions to microblog by Steve Layton](https://shindakun.dev/posts/adding-webmentions-to-microblog/)
* [Webmentions on a static site with GitHub Actions by Sebastian De Deyne](https://sebastiandedeyne.com/webmentions-on-a-static-site-with-github-actions/)

If you have any trouble setting this up, or just want to chat, feel free to reach out to me on [Twitter](https://twitter.com/hugojmd) or [Mastodon](https://melb.social/@hugo).

