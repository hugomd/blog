+++
date = "2015-10-12T22:12:05+11:00"
draft = false
title = "Monash Ninja: what, how, and the end result"
slug = "monash-ninja"
image = "http://i.hugo.sx/longterm/97TbG.png"
aliases = [
	"monash-ninja"
]
tags = ["development", "project", "writeup"]
+++

### What is Monash Ninja?
[Monash Ninja](http://monash.ninja) was week-long treasure-hunt-like event (14 Sept. to 21 Sept. 2015) run by [Wired Monash](http://wired.org.au/). The concept and code was created by [me](http://hugo.sx/) (then Vice President) and the design for the website was created by [Terence](http://terencehuynh.com/) (then Secretary).  

I purchased the _monash.ninja_ domain in April 2015 on a whim and then decided to come up with an idea for it. I had been thinking it would be fun for Wired to run a treasure hunt but thought it would be difficult to find participants as treasure hunts require effort for the hunters, but also for the organisers. This lead me to the idea of putting up (read: hiding) codes around campus (with help from Terence) that could then be entered on a website. Easy participation, a "keep your eyes peeled" approach, rather than actively looking for clues, and fairly easy implementation and organisation.


### Design and Implementation
Monash Ninja consists of a simple HTML + JQuery/Ajax + CSS frontend with a [Flask](http://flask.pocoo.org) + MySQL backend.  

The database contained 30 codes and had the following schema:
- code\_id INT  
- code VARCHAR  
- used BOOL  
- student_id INT  

The API was super simple:
- /all - JSON of entire database (only for local use)  
- /get - given a code and a student ID, validates the code and returns success/failure.  

If you're interested in seeing the code, it's on the Wired Monash Github, [here](https://github.com/wiredmonash/monash.ninja).  

Poster design:
![](http://i.hugo.sx/longterm/K4ENY.jpg)  

Website design:
![](http://i.hugo.sx/longterm/udf9y.png)  

The prizes were worth over $500 as a result of [UNIHACK](http://unihack.net/)'s success:
- 16x MSA vouches ($15 ea.)  
- 4x Google Cardboard ($20 ea.)  
- 10x iTunes Gift Vouchers ($30 ea.)  

### End Result
By the end of the week 26 of the 30 codes had been found and validated, of which around 2 were ripped down, presumably by university staff. To remedy this, we hid these two in the source code of wired.org.au and monash.ninja. Most of the prizes were collected on the following Monday after the event. Some participants were unable to collect their prizes and scheduled alternative times, while others did not respond.  

I'm pleased with the success of Monash Ninja and hope that it can continue to be run each year under the Wired Monash banner.

I'd like to thank Terence for putting up with my stupid questions, helping me distribute and hide the codes, give out the prizes and for allowing me to sticky tape a ninja code to his back for the duration of the club's Annual General Meeting (AGM).  

### What would I do next time?
Find an easier way to keep track of people. Specifically, taking emails, or using Facebook login, instead of just a student ID and code - it would have made it a little easier to notify people they'd received a prize.  

Store more information on the database: whether prizes have been given out, website feedback, and general statistics such as how many codes validated each day.

### When will Monash Ninja return?
Hopefully next year! <sup><sup>Maybe during O-week?? (hint hint)</sup></sup>
