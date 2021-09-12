---
title: Rendering static tweets
date: 2021-03-26T23:00:00+11:00
description: "Avoid using external embeds in favour of building them statically and rendering them as HTML."
tags: ["100DaysToOffload"]
sequence: 5
---

{{% 100daystooffload %}}

*Update (12/09/21): I wrote an update to this post [here](/post/update-rendering-static-tweets/), which describes a much less hacky way of doing this*.

I recently wrote about [shortcodes](/post/custom-hugo-shortcodes), describing a way to avoid duplicating text all over the place.

Delving into shortcodes got me thinking about static rendering. In an ideal world, this site would be completely static, it wouldn't call out to third-party sites to fetch data. 

I've done a reasonable job of removing analytics, external fonts, and embeds. However, there are still cases where I _do_ want to embed something from a third-party. It would be nice to bake these embeds into static output, so we're only loading from external sources at build time. If someone visits this blog, they'll still see the embedded content, but it won't be fetched from a third-party.

Scanning through my post archive, I've mostly been embedding tweets. Static rendering and tweets reminded me of a [neat Next.js demo](https://static-tweet.vercel.app). This demo renders tweets on first load and subsequently stores them statically at the edge.

Inspired by this, I've attempted to replicate that functionality on this very blog.

The tweet below might look like it's embeded via Twitter, but it's actually not. I've written a [_very hacky_ shortcode](https://github.com/hugomd/blog/blob/4c9554dda22e612dc511ce69ced3cc0006d541d9/layouts/shortcodes/statictweet.html) that fetches tweets as JSON via a [local web server](https://github.com/hugomd/blog/blob/4c9554dda22e612dc511ce69ced3cc0006d541d9/twitter-proxy/index.js). Unfortunately, [Hugo](https://gohugo.io) (the static site generator) doesn't support adding authorisation headers to requests, to facilitate calling the Twitter API directly, although [there is discussion](https://github.com/gohugoio/hugo/issues/5617#issuecomment-801767375) about making it a feature. For now, I have to proxy requests, add the authorisation header, and then forward the request to Twitter.

{{<statictweet "1310553323527745537" >}}

Here is what the shortcode looks like:

{{< highlight markdown >}}
{{</* statictweet "1310553323527745537" */>}}
{{< / highlight >}}

This shortcode is still experimental, and doesn't do nice things like rendering links in tweets, so let me know if you see anything funky.
