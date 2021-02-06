---
title: "The Fast Web"
date: 2021-02-02T07:45:39+11:00
description: "The web should feel fast, and my blog should, too!"
tags: ["100DaysToOffload"]
cover: "cover.png"
useRelativeCover: true
sequence: 1
---

{{% 100daystooffload %}}

Over the past few months I've made a few changes to my blog, prompted by [this comment](https://lobste.rs/s/h9xgpv/rate_my_homepage#c_jvxqal) on Lobste.rs. This part really got me going: "Definitely feels more like “underperforming CMS with database” than static site."

Since then, I have:

* Moved from DigitalOcean Kubernetes to Vercel
* Updated the [theme](https://github.com/panr/hugo-theme-hello-friend) to the latest release
* Made some generic performance improvements, based on [Lighthouse reports](https://lighthouse-dot-webdotdevsite.appspot.com//lh/html?url=https%3A%2F%2Fhugo.md%2F)

Most of this has been to reduce load time, so that the site should load instantly.

As much as I love messing around with Kubernetes and having a complicated pipeline to deploy changes to my blog, there's quite a few downsides:

* The [complicated pipeline](https://github.com/hugomd/blog/blob/e52fd9fe081189f1481230a3f25371cd0dfd9b46/.github/workflows/main.yml)
* Unnecessarily large container images. Despite this being a static site, if I'm using Kubernetes, I still need to use a container to serve the files. I'm using nginx to do this, which also lets me do some fancy [URL rerouting](https://hugo.md/post/json-resume-curl/). Ultimately, this means requests are routed via a load balancer, to an nginx ingress deployment, and then to another nginx container that serves static files. That's a lot of effort!
* A more complex process means I focus less on content. You'll notice I only managed three posts last year.
* My Kubernetes nodes are hosted in a single data centre in Singapore, so content isn't available at the edge. Some visitors will inevitably experience poor load times.

Vercel is quite the opposite by comparison:

* Simple pipeline. All I had to do was log into Vercel with GitHub and click a few buttons to set up a new deployment. It worked out I'm using [hugo](https://gohugo.io) on its own, and there was zero configuration. I now get a free deployment for every commit, and even pull request previews.
* Build times are astonishingly fast. Where I used to have to wait up to 4 minutes for a build, I now wait roughly 30 seconds.
* Edge caching. Vercel deployments get pushed out along their [edge network](https://vercel.com/docs/edge-network/overview). Regardless of where a visitor is, this blog should load quickly.

So, what do you think of the load times? Is there anything else I should change? Let me know via [Twitter](https://twitter.com/hugojmd), [Mastodon](https://melb.social/@hugo), or email