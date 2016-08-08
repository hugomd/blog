+++
date = "2014-01-20T09:57:55+11:00"
draft = false
title = "I'm an Idiot"
descriptions = "TLDR; I overthought things"
slug = "im-an-idiot"
aliases = [
	"im-an-idiot"
]
+++
I've been working on a pretty big project on Github with a couple of developers. It has a lot of potential and requires a lot of work.  

We happen to be using a [Content Delivery Network](http://en.wikipedia.org/wiki/Content_delivery_network) (CDN), because we're hosting files.  

I thought I would be smart, and route all traffic via the server before routing to the CDN. This would then allow me to restrict access to the files so that they could only be accessed by specific referers (specific IP addresses or websites). Sounds great, right? **Wrong.** By doing this, the CDN would be rendered useless because all traffic would go through the main server, then the CDN, rather than straight to the CDN.  

I'm an idiot.
