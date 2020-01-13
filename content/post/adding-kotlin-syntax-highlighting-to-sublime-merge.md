---
title: "Adding Kotlin syntax highlighting to Sublime Merge"
date: 2020-01-13T19:02:50+11:00
description: "💅💅💅"
tags: ["git"]
---

1. Download `Kotlin.tmLanguage` from [here](https://github.com/vkostyukov/kotlin-sublime-package/blob/ce599a846ac9f86d4671a024d21220ca08e50f66/Kotlin.tmLanguage)
2. Open Sublime Merge, click Preferences -> Browse Packages
3. Create a folder called `Kotlin` and place `Kotlin.tmLanguage` within the folder
4. Restart Sublime Merge and enjoy! 🎉


Alternatively, if you want to use terminal and you happen to be running **macOS**, you can run:
```bash
cd /Users/$USER/Library/Application\ Support/Sublime\ Merge/Packages/ \
  && mkdir Kotlin \
  && cd Kotlin \
  && wget https://github.com/vkostyukov/kotlin-sublime-package/raw/ce599a846ac9f86d4671a024d21220ca08e50f66/Kotlin.tmLanguage
```
