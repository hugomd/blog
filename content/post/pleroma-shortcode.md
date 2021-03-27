---
title: "Rendering static Pleroma posts"
date: 2021-03-27T20:31:18+11:00
description: "You thought a static Twitter shortcode was cool, how about one for Pleroma?"
tags: ["100DaysToOffload"]
sequence: 6
---

{{% 100daystooffload %}}

Following on from [yesterday's post about static tweets](/post/render-tweets-statically), I've also added a shortcode for fetching posts directly from Pleroma[^1]. You can check out the code [here](https://github.com/hugomd/blog/commit/bb82a171e2fc1104726e0a25a8e5e746ee5342c2). 

Unlikey for the Twitter shortcode, I don't have to proxy requests, because the Pleroma API does not require authorisation for public posts, making the implementation much less convoluted.

Check out the example below:
{{< pleroma "soykaf.org" "A5bLRz1f4U8hk8SPfE" >}}

[^1]: [Pleroma](https://pleroma.social) is an alternative to Mastodon, with a similar feature set, also using [ActivityPub](https://activitypub.rocks).
