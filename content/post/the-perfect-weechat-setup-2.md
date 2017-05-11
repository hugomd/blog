+++
date = "2014-07-26T13:41:12+10:00"
draft = false
title = "The Perfect Weechat Setup"
description = "Everything terminal is awesome"
slug = "the-perfect-weechat-setup-2"
aliases = [
	"the-perfect-weechat-setup-2"
]
tags = ["development", "setup", "tutorial"]
+++
### Update 13/08/15
I've been getting a few emails asking for my configuration files so I've uploaded them: [weechat.conf](http://s3.hugo.sx/files/weechat/13-07-15/weechat.conf), irc.conf (temporarily unavailable). "weechat.conf" contains general configurations for looks but "irc.conf" contains a setting that lets you create indented channels, under their respective server.

### What we'll be doing
1. Important things to know
2. Installing [Weechat](http://weechat.org/).  
2. Installing plugins
3. Making it look pretty.
4. Setting up [ZNC](http://wiki.znc.in/ZNC) which lets you stay connected to IRC indefinitely.

### Prior knowledge
Before we get started, it's vital that you have some command line experience, whether it's a little bash here and there or programming. I say this not to dissuade you from installing weechat but because if you don't like CLI (Command Line Interface), you'd be far more comfortable using a GUI (Graphic User Interface).  

##### Weechat/IRC terminology
- `buffers` are essentially 'windows' in which messages/chats/settings options are shown. You can generally switch between these using `meta-[1 .. 9]`. The first buffer, labelled 1, contains connected server buffers by default, that's the servers you're connected to. You can switch between these servers using `Ctrl+X`, that's *not* the same as switching between buffers.
- key bindings - escape is the 'meta' key on Mac. 
- `/query` and `/msg` do different things - `/query` opens a new buffer in which you can type messages to users while `/msg` will send a direct message to a user without opening a buffer.
- the weechat configuration system runs on a specific hierarchy (it's quite smart) - specific settings are denoted by the label they start with. For instance, general IRC settings are denoted by `irc` while weechat settings are denoted by `weechat`. This is generally useful to know when modifying configuration from within weechat, which you can do using `/set [options]`.
- Browsing options - you can use the iset.pl plugin to search for key terms in configuration or you can use `/set weechat.*` and `/set irc.*` to view settings available. You can use the `*` symbol to list all options available in that hierarchy.

Keeping these points in mind, we can begin our install and setup.

### Install
Head over to [weechat.org](http://weechat.org/) and download it from source or just use the repositories available on most linux distros:  

	sudo apt-get install weechat
    
You can also use [homebrew](http://brew.sh/) on Mac.http://brew.sh/.  

	brew install weechat --with-aspell --with-curl --with-python --with-perl --with-ruby --with-lua --with-guile

Or even [Macports](https://www.macports.org/)

	sudo port install weechat
    
On Windows, you can use [Cygwin](http://www.cygwin.com/), although I haven't personally experiemented with it.  

More information on installing can be found in the [weechat documenation](https://weechat.org/files/doc/stable/weechat_user.en.html#install).

### Plugins
Now onto one of my favourite parts about weechat. The plugins. Weechat has a whole range of plugins for just about everything you can and can't think of. Most of the scripts are available [here](http://weechat.org/scripts/) and those that aren't are likely on Github.  

The scripts that I currently use are:    

- [buddylist.pl](http://weechat.org/scripts/source/buddylist.pl.html/)
	- creates a buddy list of select nicknames and tells you if they're online. Also allows you to easily query/message them  
- [buffers.pl](http://weechat.org/scripts/source/buffers.pl.html/)  
- [iset.pl](http://weechat.org/scripts/source/iset.pl.html/)  
	- settings browser
- [notification_center.py](http://weechat.org/scripts/source/notification_center.py.html/)
	- show [notifications](http://i.sqr.cat/VeZN/+) on Mac
- [prism.py](http://weechat.org/scripts/source/prism.py.html/) 
	- send rainbow coloured text
- [text\_item.py](http://weechat.org/scripts/source/text_item.py.html/)  
	- useful when adding custom text to the status bar
- [urlserver.py](http://weechat.org/scripts/source/urlserver.py.html/)  
	- if an URL goes across two lines in weechat, it has a tendency to break the link for me, this plugin automatically shortens all URLs on a localhost.
- [shell.py](http://weechat.org/scripts/source/shell.py.html/)
	- lets you run shell commands from weechat and pipe them to chat. (My favourite is `/shell -o toilet -F border -f future --gay --irc "This is pretty cool..."`)

Most of these plugins are fairly easy to install, you can just use the following command:  

	/script install [plugin]

Or, if the plugin isn't available in the weechat repository, you can download it and place it in the `~/.weechat/plugins` folder.  

Typing `/script` will give you a list of available plugins to install - you can then use the arrow keys to navigate through the list and then type `i` followed by the enter key to install the selected plugin.


### Making it look pretty
There's a lot you can do to weechat to make it look the way you want but I personally love the amount of colour-related customisation  available. I explained how the settings worked in _Weechat/IRC terminology_ section and while you can choose to use `/set weechat.*` to find all of the settings, it's significantly easier to use the iset plugin.

To use iset, simply go to any buffer and type `/iset`, this will open up a new buffer with a long list of all the weechat settings. From there, you can type in the name of a setting you might want to change or simply browse through the list of settings. What makes iset especially useful is the fact that it will explain what the settings change. 

I'm currently using toggles so that I can hide certain parts of the interface when I want to, including the nicklist, the topic, the status and the buffer list. This [weechat wiki page](http://weechat.org/files/doc/devel/weechat_user.en.html#screen_layout) does a very good job of explaining the screen layout, which will be good to know when you're changing settings.

Toggles are fairly simple to setup, for example, to toggle the buffer bar, press `Esc + B`:

	/key bind meta-b /bar toggle buffers
    

My weechat currently looks like this:
![Weechat 1](http://i.hugo.sx/ss/4o5bT.png)

The main changes I've made are:
  
    /set weechat.look.buffer_time_format
    /set weechat.look.prefix_align “right”
    /set weechat.look.align_end_of_lines message
    /set weechat.look.prefix_same_nick “↳”
    /set weechat.look.prefix_join “—>”
    /set weechat.look.prefix_quit <—”
    
    /set weechat.look.buffer_time_format “%H:%M”
    /set weechat.look.prefix_align “none”
    /set weechat.look.align_end_of_lines “message”
    /set weechat.look.prefix_same_nick “”
    /set weechat.look.prefix_join “”
   
    /set irc.look.display_host_quit off
    /set irc.look.display_host_join off
    /set irc.look.display_host_join_local off
    /set irc.look.color_nicks_in_server_messages off
   
    /set buffers.look.show_number off
	—
	/set weechat.look.read_marker none

### Setting up ZNC
[ZNC](http://znc.in/) is what's called an IRC bouncer and is particularly useful if you want to remain connected to an IRC server all the time. It's nice to be able to connect to a server and have some 'history' of the channel; you can see what people have been discussing and then add to the conversation immediately. It also allows other users to leave you a message.

[DigitalOcean](https://www.digitalocean.com/) has a particularly [good guide](https://www.digitalocean.com/community/tutorials/how-to-install-znc-an-irc-bouncer-on-an-ubuntu-vps) on setting up ZNC that should work on most servers or on a home computer. 

Now that I look back at the top paragraph I realise I haven't explained ZNC all that well. The essence of it is, you run a server that connects to IRC for you, then, when you want to use IRC, you connect via that server.  

If you can't afford to pay $5 a month to run ZNC, haven't the know-how with Linux to set it up, can't be bothered or don't have a homeserver you can use, shoot me a message on [Twitter](http://twitter.com/hugojmd) or elsewhere and I'd be happy to set up an account for you on my own server.
