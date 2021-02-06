---
title: Editing in GitHub Codespaces
date: 2021-02-06T07:45:39+11:00
description: "Using GitHub Codespaces to edit this blog ✨"
tags: ["100DaysToOffload"]
sequence: 2
---

{{% 100daystooffload %}}

I wrote this post in [GitHub Codespaces](https://github.com/features/codespaces), which looks like Visual Studio Code but in the browser (it even supports Vim mode!).

You can set up a new Codespace by logging into GitHub and navigating [here](https://github.com/codespaces), then creating a new codespace for your repository.

In my case, I chose my blog repository: [`hugomd/blog`](https://github.com/hugomd/blog). GitHub spun up a new codespace for me, with `hugo` version 0.76.3, which is pretty new. It also includes `docker` and other helpful tools by default.

Once you start running `hugo serve`, you will be prompted to forward port `1313` (the default `hugo` port). Unfortunately, this means that when you load up your blog, it will load without any resources, because all requests will be routed to `localhost:1313` by default, rather than to the codespace.

Thankfully, there's an easy fix to this:
```bash
hugo serve --baseUrl=/ --appendPort=false 
```

The above command overrides the `baseUrl` from `config.toml`, which is `https://hugo.md/` in [my config](https://github.com/hugomd/blog/blob/develop/config.toml#L2). The last part is key, it avoids appending the port, `1313` to the `baseUrl`.

Now you can browse to the link (`*.apps.codespaces.githubusercontent.com`) that was forwarded, and your `hugo` blog should load up with all its resources!