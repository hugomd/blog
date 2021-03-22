---
title: Custom shortcodes
date: 2021-03-22T07:45:39+11:00
description: "A nice way to simplify duplicate copy across posts on my blog."
tags: ["100DaysToOffload"]
sequence: 4
---

{{% 100daystooffload %}}

I've known about [custom shortcodes in Hugo](https://gohugo.io/templates/shortcode-templates/) for awhile, but I've never had the need to use them.

Now that I'm taking part in 100DaysToOffload, it makes sense to create one, because it makes it less cumbersome to write posts. I only have to make changes to [one file](https://github.com/hugomd/blog/blob/fa29125bafd3206299ff712687c33d435da880ed/layouts/shortcodes/100daystooffload.html) to update copy across all of my related posts.

The implementation is concise, you create a new HTML file in `layouts/shortcodes`, for example:
```
<!--100daystooffload.html-->
This is post number {{ $.Page.Params.sequence }} in my [100 Days To Offload](https://100daystooffload.com/) challenge.
[View all posts](/tags/100daystooffload/) or [subscribe via RSS](/tags/100daystooffload/index.xml).

---
```

Then you can refer to the shortcode in your posts using the filename excluding the extension:
```
{{%/* 100daystooffload */%}}
```

The reference to `$.Page.Params.sequence` is replaced with the `sequence` variable defined in the [frontmatter](https://gohugo.io/content-management/front-matter/) for the post. For each new post I write, I manually set the `sequence` accordingly. This post, for instance, is number 4.
