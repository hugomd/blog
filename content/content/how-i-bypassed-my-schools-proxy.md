+++
date = "2014-02-11T11:18:56+11:00"
draft = false
title = "How I bypassed my school's proxy"
slug = "how-i-bypassed-my-schools-proxy"
aliases = [
	"how-i-bypassed-my-schools-proxy"
]
tags = ["story", "writing"]
+++
In my final year of schooling, I worked out how I could bypass my school's proxy. A shame that I discovered it so late, but hopefully those reading this may be able to get a bit more use out of it than I did (only a few months!).  

This idea/hack will really depend on your school's proxy system but it should, theoretically, work on most networks (don't quote me on this). My school happened to be using [Fortiguard](https://www.fortiguard.com/), which tended to block the usual things like Facebook, YouTube, etc.  

**Requirements**	

+	A fast internet connection at home (1 Mbps up, at least).  
+	A fast internet connection at school, if it has a high down speed, it'll increase the speed with which web pages load through the tunnel we'll be setting up.  
+	Access to a Linux system at home. This could be running Linux on an old laptop at home or, in my case, having a [Raspberry Pi](http://raspberrypi.org/).  
+	Ability to open specific ports through your ISP.  

**Let's do this.**  

1. Open a port through your ISP and then in your router. This usually involves logging into your ISP's administration website and opening ports. I was only able to open port 443, 22, and a few others. For this hack, ideally you'll want to open port 443, that way your school won't be able to block you. After you've done that, you should be able to navigate to your WiFi router at http://192.168.1.1/, you can find out what the URL is [here](http://portforward.com/default_username_password/). Forward the port you opened through your ISP, on your local network, for the Linux server/computer you'll be connecting to. You'll also want to [check whether the port is open](http://www.canyouseeme.org/).  
2. Once you've completed those relatively easy steps, we can move onto the fun part. When you're connected to your school network, you can create an SSH tunnel to your server at home:  


		ssh -p 443 -ND 8080 root@YOURIP
        
	In the above code, 443 is the port you forwarded and 8080 is the one you'll be routing local traffic through. 'YOURIP' is just your computers IP (not the local one).  
    
    I'll be updating this blog post with further information on how to route local traffic through the tunnel on Mac OS X, Linux and Windows.
