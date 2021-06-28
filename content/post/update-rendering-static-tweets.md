---
title: "Update: Rendering Static Tweets"
date: 2021-06-28T15:45:58+10:00
description: "Statically rendering tweets is now even easier."
sequence: 10
tags: ["100DaysToOffload"]
---

{{% 100daystooffload %}}

A few weeks ago, I wrote a post about [rendering static tweets](/post/render-tweets-statically). I've made a few important improvements, worthy enough of a new post.

In my original post, I described using a proxy to inject Twitter API headers. This is hacky, and leads to flaky builds, because the build server needs to handle a NodeJS proxy running in the background[^0]. I got this working on Vercel and Cloudflare Pages[^1], but there was no guarantee the build would pass, and I was constantly re-running failed builds. Neither fun nor productive. In the past few weeks I've wanted to remove this functionality from my blog because it's made it more difficult to preview and deploy.

Last week I was reading [Nicholas Whittaker's blog](https://nicholas.cloud/), specifically a post he wrote about [static embeds for tweets and videos](https://nicholas.cloud/blips/static-embeds-for-tweets-and-videos/) (sounds familiar, right?). He's using [workers](https://github.com/nchlswhttkr/workers) to handle some of the proxying, but notably, he's not calling Twitter's private API, which requires authentication, he's calling a _public_ one. 

I had no idea `https://cdn.syndication.twimg.com/` existed. It's a completely open API you can use for fetching tweets, which means it's perfect for static sites!

Try it out: `curl "https://cdn.syndication.twimg.com/tweet?id=1406108535356678145"`

Check out the updated shortcode [here](https://github.com/hugomd/blog/blob/6ad96b24117255c2a9912c566ffd081bd9bbd6f1/layouts/shortcodes/statictweet.html).

After some further sleuthing on Nicholas' blog, I realised you can [configure caches](https://gohugo.io/getting-started/configuration/) in Hugo. This means you can cache calls to `getJson`, and push the cache to git. If a tweet ever gets deleted, as long as it exists in the cache[^2], I can continue rendering the tweet. Neat!

[^0]: This sounds more complicated than it is. The Hugo shortcode makes a request to `localhost:8080/{tweetId}?auth={authToken}`, the proxy then calls `https://api.twitter.com/1.1/statuses/show?id=${tweetId}` with the same tweet ID and an authentication header. The proxy is [on GitHub](https://github.com/hugomd/blog/blob/e7e49ac6204562e43360798c17d1c2175cab8a2a/twitter-proxy/index.js) if you want to take a peek.


[^1]: Here is what the build command looked like: `cd twitter-proxy && npm install && node index.js & sleep 5 && echo "Done" && hugo -t hello-friend --baseUrl=$BASE_URL --ignoreCache`

[^2]: Provided you've set `maxAge: -1`, so the cache is never purged. My config is [here](https://github.com/hugomd/blog/blob/04c72df2603979d2b1af9a642ed973951d2d6bb0/config.toml#L9-L12).
