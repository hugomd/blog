---
title: "Android KitKat Native Screen Recording"
date: "2013-11-01T22:16:59+11:00"
slug: "android-kitkat-native-screen-recording"
aliases: ["android-kitkat-native-screen-recording"]
tags: [ "android", "tutorial" ]
---

I've just tried it out and it is absolutely awesome!

Before I start, I'd like to thank Android Police for the initial information on how to do it, in [this article](http://www.androidpolice.com/2013/10/31/kitkat-feature-spotlight-android-4-4-includes-native-screen-recording-with-mp4-output/).

There are two ways to go about this, the first is to use a terminal emulator (you'll need root) on your phone to execute the commands and the second is to download ADB and execute the commands through your terminal.  

Additionally, the command has the following paremeters, the notable limitation being that it can only record for a maximum of 3 minutes.   

{{< highlight bash >}}
Usage: screenrecord [options] <filename>

  Records the device's display to a .mp4 file.
  
  Options:
  --size WIDTHxHEIGHT
      Set the video size, e.g. "1280x720".  Default is the device's main
      display resolution (if supported), 1280x720 if not.  For best results,
      use a size supported by the AVC encoder.
  --bit-rate RATE
      Set the video bit rate, in megabits per second.  Default 4Mbps.
  --time-limit TIME
      Set the maximum recording time, in seconds.  Default / maximum is 180.
  --rotate
      Rotate the output 90 degrees.
  --verbose
      Display interesting information on stdout.
  --help
      Show this message.

Recording continues until Ctrl-C is hit or the time limit is reached.
{{< / highlight >}}
    
Further documentation is available [here](http://developer.android.com/tools/help/adb.html#screenrecord).

---
#### Method 1 - Terminal & Super User
Make sure you've applied [ChainFire's superSU](http://download.chainfire.eu/351/SuperSU/UPDATE-SuperSU-v1.65.zip) and have a rooted device. Download a terminal emulator, I used [this one](https://play.google.com/store/apps/details?id=jackpal.androidterm).  

Open up the terminal emulator and type the following:
	
{{< highlight bash >}}
su
{{< / highlight >}}
 
A little SuperUser popup should appear, grant permission. This will give the terminal root access. Next type this in:
 
{{< highlight bash >}}
screenrecord /sdcard/FILE.mp4
{{< / highlight >}}
    
Now you can make your screen recording. Once you've finished, head back to the emulator and press Volume Down + C, a little <code>^C</code> should appear, then press enter. You can then upload your screen recording via Dropbox or another app, or just transfer the file over USB.

---
#### Method 2 - ADB

Download ADB (Android Debug Tool) which you can download from this [XDA thread](http://forum.xda-developers.com/showthread.php?t=1474956), alternatively download it directly from [here](http://four-nineteen.com/veronica/XDA%20Developers/Nook%20Tablet/DRIVERs/ADB%20+%20Fastboot%20+%20Drivers.zip). Neither of those are 'proper' downloads. If you want to download directly from Google, you'll need to download the [SDK](http://developer.android.com/sdk/index.html).  

Okay, once that's done, it's pretty simple. Plug in your phone via USB, enable USB Debugging on your phone. Then open the folder containing ADB in Terminal and enter the following:

{{< highlight bash >}}
./adb shell screenrecord /sdcard/FILE.mp4
{{< / highlight >}}

Once you've done that, it will begin recording. It might also be a good idea to go into Developer Options and enable 'Show Touches', so that people can see what you're touching.  

When you've finished recording, hit Ctrl+C in Terminal to end the process, then use the following command to download the file:

{{< highlight bash >}}
./adb pull /sdcard/FILE.mp4
{{< / highlight >}}
    
I tried it out myself;
<iframe width="420" height="315" src="//www.youtube.com/embed/Uc_BDxcLikE" frameborder="0" allowfullscreen></iframe>
