---
title: "Theme-aware syntax highlighting"
description: "Syntax highlighting themes that adapt to light and dark modes with the static site generator Hugo."
date: 2021-08-29T12:38:46+10:00
tags: ["100DaysToOffload"]
sequence: 12
---
{{% 100daystooffload %}}

{{< sidenote "Hugo" 1 >}}I keep having to specify, so it doesn't seem like I'm referring to myself in third person.{{< /sidenote >}} (the [static site generator](https://gohugo.io/)) supports static syntax highlighting.

Instead of using a JavaScript based highlighter, you can perform highlighting
at build time, rather than pulling in yet another dependency to slow down your
site.

There's great documentation for it [here](https://gohugo.io/content-management/syntax-highlighting/).

Unfortunately, it doesn't support generating theme-aware CSS. If someone is 
viewing this blog in dark mode, they'll see a light style, or vice-versa 
depending on the syntax highlighting theme.

Thankfully, there's a nice way around this: media queries!

Here's a short video to demonstrate what I'm talking about:
<video controls width='100%'>
	<source src='./syntax-light-dark.mp4' type='video/mp4'>
	Your browser doesn't support the HTML5 video tag :(
</video>

When in light mode, the syntax theme is solarized. When dark mode is toggled,
the theme switches to solarized-dark.

To achieve this, we first need to generate both light and dark styles:
{{< highlight bash >}}
hugo gen chromastyles --style=solarized-dark > syntax-dark.css
hugo gen chromastyles --style=solarized > syntax-light.css
{{< /highlight >}}

You can find a list of available styles 
[here](https://help.farbox.com/pygments.html).

Once that's done, we can combine them into one file:

{{< highlight css "hl_lines=2 5,linenostart=1" >}}
/* file: syntax.css */
/* Paste the default syntax CSS here, e.g. solarized-light */

@media screen and (prefers-color-scheme: dark) {
  /* Paste the dark syntax CSS here, e.g. solarized-dark */
}
{{< /highlight >}}

All that's left to do is import the above snippet into your 
theme's `<head>` template, like so:

{{< highlight html >}}
{{ $syntax := resources.Get "css/syntax.css" | minify | fingerprint }}
<link rel="stylesheet" href="{{ $syntax.Permalink }}" integrity="{{ $syntax.Data.Integrity }}">
{{< /highlight >}}

The above snippet assumes `syntax.css` is stored in `assets/css/`.
