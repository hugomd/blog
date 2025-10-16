---
title: "Secret environment variables with Hermit and 1Password"
date: 2025-10-16T22:49:39+11:00
draft: false
tags: [development]
---

This post is a quick {{<sidenote "TIL" >}}"Today I Learned"{{< /sidenote >}} for posterity.

I'm building a {{<sidenote "side project" >}}It's called [FeedSync](https://feedsync.net), and it solves a _very_ niche problem: subscribing to RSS feeds, and piping new entries into Discord.<br /><br /> I'd love it if you tried it out ❤️{{< /sidenote >}}, and I'm contributing from two machines: my MacBook Pro, and my Linux desktop.

### The problem
1. I need secrets on both machines.
2. I don't want to over-engineer by committing encrypted secrets to the git repository.
3. I don't want to store secrets somewhere like `~/.zshrc` unencrypted.

### The solution

[Hermit](https://cashapp.github.io/hermit) initialises the environment, and 1Password injects the secrets.

In 1Password Desktop, I created a new vault called `Development`, a new document called `FooService`, a section called `dev`, and a {{<sidenote "password" >}}I used a password because it's a secret value, but you can use other types as well.{{</sidenote>}} called `FOO_SECRET`. The [1Password documentation](https://developer.1password.com/docs/cli/secret-references) goes into more detail.

In the terminal, I installed [1Password CLI](https://developer.1password.com/docs/cli/), ran `hermit init`, and updated `bin/hermit.hcl` to:

{{< highlight hcl >}}
env = {
  "FOO_SECRET": "op://Development/FooService/dev/FOO_SECRET"
}
{{< /highlight >}}

My app already makes use of environment variables like `FOO_SECRET`, so there's no change there.

To run my application with injected secrets on either of my machines, I can use this command:

{{< highlight bash >}}
op run -- iex -S mix phx.server
{{< /highlight >}}
