---
title: "Self-hosting"
date: 2021-12-04T00:00:00+00:00
tags: ["100daystooffload"]
description: "Hosting <s>everything</s> some core services yourself."
---

## What is self-hosting?

Self-hosting means running services yourself, on your own infrastructure. For me, this means hosting, amongst other things, a media server, and [Network Attached Storage (NAS)](https://en.wikipedia.org/wiki/Network_attached_storage) on a small form factor computer.

## Why?

Self-hosting is often a appealing for a few reasons:

- Cost
- Privacy
- Fun
- Self-sovereignty

Let's break these down.

### Cost

Cloud-based services are usually expensive. Even if it has become ubiquitous to host static websites for free, that's not the case for services like email, micro-blogging, file hosting, media streaming, or anything requiring reasonable resources.

Let's do a cost breakdown and some amortisation.

A basic server at your average cloud provider with:
- 1 GB of memory
- 1 vCPU
- 25 GB of SSD storage
- 1 TB of bandwidth

This costs $6 USD a month, or $66 USD a year. 

A Raspberry Pi 4 with similar specifications costs ~70 USD a year. After a year of renting a small server from a cloud provider for a year, you may have been better off buying a Raspberry Pi 4 and hosting it at home.[^0]

TODO: monthly bills

### Privacy

Using hosted services puts you at the mercy of the company providing the service. This means they, or the government in their jurisdiction, may access your data at will, without seeking your consent, and sometimes without informing you about it.

### Self-sovereignty

### Fun

If you're interested in technology, systems administration, etc, it can be quite fun hosting your own services, maintaining uptime, and managing upgrades.

The flip side of course, is that you have to maintain it even when you don't want to, or don't have the time to. 

This effort compounds if you provide access to friends and/or family, who will message you when things break.

## What do I self-host?

- Plex
- Homer
- Qbittorrent
- Whoami
- Grafana
- Home Assistant
- Authelia (now replaced by Cloudflare)

## What _don't_ I self-host?

TODO: deciding what to host vs. what not to host.

## In the Cloud vs. at Home

TODO: what do I host in the cloud vs. at home

[^0]: I'm aware this is not a reasonable comparison, as it does not take into account cost of bandwidth, electricity, maintenance, and other operational costs.
