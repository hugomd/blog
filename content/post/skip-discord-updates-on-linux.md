---
title: "Skip Discord Updates on Linux"
date: 2024-05-18T16:48:13+10:00
tags: ["linux"]
---

If you use Discord on Linux, and installed via a `.deb`, you might have seen
this annoying pop up every time you start Discord:

<img style="margin: auto;" alt="A screenshot of a Discord update prompt" src="discord-update.png" />

This seems like Discord *requires* you to upgrade before you can start the
client, but that's not the case.

To skip updates on start up, add the following key to `~/.config/discord/settings.json`:

{{<highlight json >}}
"SKIP_HOST_UPDATE": true
{{< /highlight >}}

Next time you start Discord, it won't check for updates.


