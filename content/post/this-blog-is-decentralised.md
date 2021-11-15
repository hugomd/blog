---
title: "This blog is decentralised"
date: 2021-09-12T19:33:06+10:00
description: "Available on HNS and ENS, via IPFS."
tags: ["100DaysToOffload"]
sequence: 13
---

{{% 100daystooffload %}}

This blog is now available on the Handshake blockchain via [blog.hugomd](http://blog.hugo.md) and on the Ethereum Name Service (ENS) via [hugomd.eth](http://hugomd.eth) (or [hugomd.eth.limo](https://hugomd.eth.limo) if you don't have an ENS resolver).

Why? 

I've seen a few people on Twitter sporting `$name.eth` handles, and thought I would see how ENS has matured since I last looked into it. I've been an avid supporter of Handshake they released their airdrop, and have a few TLDs there already, why not check out an alternative and hedge my bets?

I used [Fleek](https://fleek.co/) to deploy to IPFS, which was almost as streamlined as Cloudflare Pages. You sign up, link your GitHub account and a repository, configure the builder you want to use, and you're done.

I ran into a few issues finding a Docker image that supported `hugo` and `openring`, but I found [one eventually](https://github.com/klakegg/docker-hugo).

What are your thoughts on decentralised DNS? Let me know.
