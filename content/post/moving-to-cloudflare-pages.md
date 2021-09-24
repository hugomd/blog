---
title: "Moving to Cloudflare Pages"
date: 2021-06-13T16:59:13+10:00
description: "Computing at the edge!"
tags: ["100DaysToOffload"]
sequence: 8
---

{{% 100daystooffload %}}

Today I caught up for coffee with a good friend, [Renlord](https://renlord.com/). He's convinced me to move this blog to [Cloudflare Pages](https://pages.cloudflare.com/).

Earlier in the year, I wrote about [migrating to Vercel](/post/the-fast-web/) to improve performance by caching my blog at the "edge".

Vercel is great, and feels pretty seamless to use. I can push commits and see them deployed in about close to a minute, and load times are _fast_ no matter where you are.

From an integration perspective, Cloudflare Pages and Vercel are quite similar. You authenticate with GitHub, choose the repositories you want to share, and Cloudflare Pages will pick up on your static site generator (in my case, [hugo](https://gohugo.io)), and offer a build command.

Where I think Cloudflare Pages shines is in further optimisation of content being served. When adding a custom domain to Cloudflare, you can enable auto minification of files JavaScript, CSS, and HTML, turn on Brotli compression [^0].

Lighthouse performance on [web.dev](https://web.dev/measure/) seems to be equivalent between both services, but there's a noticeable difference when loading from the local PoP[^1].

Here is performance from my browser via Vercel:
![Lighthouse performance for Vercel](./vercel.png)


Here is the same test against, but against Cloudflare Pages:
![Lighthouse performance for Cloudflare Pages](./cloudflare-pages.png)

We save about 0.2s on a few metrics, which isn't bad!

Now, I know what you're thinking, this is total overkill for a static site, and there's no _noticeable_ performance improvement moving from Vercel to Cloudflare Pages for the average website. That's true. I just like improving performance and playing with new technology/services at the same time. This was a great way to spend a few hours this afternoon.

Expect a follow-up post on using [Cloudflare Tunnel](https://www.cloudflare.com/en-au/products/tunnel/) to proxy traffic to my home Kubernetes cluster.

_This post was **not** sponsored by Cloudflare._

[^0]: You can read more about this [here](https://support.cloudflare.com/hc/en-us/articles/200168196-How-do-I-minify-HTML-CSS-and-JavaScript-to-optimize-my-site-).
[^1]: A PoP is a Point of Presence. Cloudflare has a page dedicated to their network [here](https://www.cloudflare.com/network/).
