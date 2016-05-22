+++
date = "2013-11-02T12:18:09+11:00"
draft = false
title = "Making a Toggle for Screen Recording in Android 4.4"
slug = "making-a-toggle-for-screen-recording-in-android-4-4"
aliases = [
	"making-a-toggle-for-screen-recording-in-android-4-4"
]
+++
Last night I made a blog post on how to screen record. This morning, I decided to make toggles with Tasker so that I could do it all from my phone easily, without a computer.  

1. Install [Tasker](https://play.google.com/store/apps/details?id=net.dinglisch.android.taskerm).  
2. Install [BusyBox](https://play.google.com/store/apps/details?id=stericson.busybox).  
3. Copy [this file](http://i.hugo.sx/UauN/download/screen.sh) to /sdcard/ with ADB;  

		./adb push screen.sh /sdcard/screen.sh
    
4. Next you can use <code>./adb shell</code> to get into your phone's terminal. 
5. Type <code>su</code>, a super user prompt should appear on your phone, grant permissions.
6. Type <code>cd /sdcard/</code> followed by <code>mkdir screens</code> and then <code>chmod 777 screen.sh</code> (not that I used 777 because it's on my phone and no one else has access, it's not normally a good idea).  

7. Now open up Tasker, go to the Tasks tab, hit the + button at the bottom right, create a 'Start' task. Tap the 'Start' task, then hit the + down the bottom, then Script and then Run Shell. 
8. Toggle 'Use Root' and set this command: <code>sh /sdcard/screen.sh</code>

9. Now create another task with the same parameters, with the name 'End'.
10. Set this shell command: <code>kill -2 $(pidof screenrecord)</code>

11. Now you can go to your homescreen, create a new Tasker wiget, select Start and End. (Note: you'll have to set an application icon for each of the tasks, this can be done by editing the task and tapping the button on the bottom right).  

12. Your screen recordings will be saved under /sdcard/screens with the filename screen_TIMESTAMP.mp4 (where timestamp is a simple <code>date +%s</code> command). You can now upload them over USB or via something like Dropbox.  

I took the liberty of uploading an example video.
<iframe width="420" height="315" src="//www.youtube.com/embed/abc-iEBWHHY" frameborder="0" allowfullscreen></iframe>

