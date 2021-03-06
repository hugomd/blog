---
title: Disabling comments
date: 2021-03-06T16:43:03+11:00
description: "If you want to comment, feel free to email me."
tags: ["100DaysToOffload"]
sequence: 3
---

{{% 100daystooffload %}}

I've disabled comments, again.

I was using Disqus comments, but removed them due to privacy concerns and to decrease load time, as Disqus was pulling in a lot of third party dependencies (see: [Disqus, the dark commenting system](https://supunkavinda.blog/disqus)).

Then I switched to [web mentions](/post/add-webmentions-to-hugo-from-micro-blog/), using Micro.blog as the source for them.

I didn't end up using Micro.blog much, so it wasn't worth the $5 a month I was paying for it. That's not to say it's not a great platform, it just doesn't work for me personally.

Since then, I've seen some great improvements to web mentions, pulling them from sources like Mastodon or Twitter. [Horst Gutmann](https://zerokspot.com) has a great post on supporting [comments via Mastodon](https://zerokspot.com/weblog/2021/01/07/comments-via-mastodon/), and maintains [`webmentiond`](https://github.com/zerok/webmentiond). If I were to set up web mentions again, I'd likely self-host `webmentiond` myself.


If you do happen to want to comment on one of my posts, feel free to [open an issue](https://github.com/hugomd/blog/issues/new), or to [contact me](/about) directly.
