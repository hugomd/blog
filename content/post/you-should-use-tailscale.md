---
title: "You should use Tailscale"
date: 2021-11-25T17:42:38+11:00
description: "I recently installed [Tailscale](https://tailscale.com/) on my [Umbrel](https://getumbrel.com/) node, so I can access it remotely without going via Tor, because Tor is *so slow*."
tags: ["homelab"]
---

I recently installed [Tailscale](https://tailscale.com/) on my [Umbrel](https://getumbrel.com/) node, so I can access it remotely without going via Tor, because Tor is *so slow*.

It's one of the few services I've used that I can say *just works*.

They also have a reasonable [hobby tier](https://tailscale.com/pricing/).

## Tailscale and Umbrel
1. Sign up [to Tailscale](https://tailscale.com/start)
2. [Install Tailscale](https://tailscale.com/download) on your Umbrel node, following the Linux instructions, and on your phone.
3. Once set up, you can go about using various Umbrel apps remotely, replacing `umbrel.local` in your connection strings with the Tailscale IP for your node (e.g. `100.123.300.4`).

I've tried this out with [BlueWallet](https://bluewallet.io/), [FullyNoded](https://fullynoded.app/), and [Zap](https://www.zaphq.io/), with {{% sidenote "great success!" %}}Although [@satsophone](https://twitter.com/satsophone) has pointed out that Zap is effectively abandonware now that [Strike](https://strike.me/) is available.{{% /sidenote %}}

Let me know if you run into any issues, and I'll help out where I can. 

If you're interested in reading more about Tailscale, checkout [How I use tailscale](https://stanislas.blog/2021/08/tailscale/) by [Stanislas](https://stanislas.blog/).
