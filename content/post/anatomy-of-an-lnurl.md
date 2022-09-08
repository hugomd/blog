---
title: "Bech32 decoding with examples"
date: 2022-08-08T17:59:31+10:00
draft: true
tags: []
---

{{% infobox %}}
This post includes some rather wide code snippets which may not render
well on smaller screens, sorry about that.
{{% /infobox %}}


* What is Bech32?
* How do you decode Bech32?
* Examples of decoding
* What is an LNURL?

{{< highlight html "linenos=false">}}
lnurl1dpjkcmr0ypmk7unysn5n70                                        
└┬──┘│└───────┬──────┘└─┬──┘                                        
 │   │        │         │                                           
 │   │        │         │                                           
 │   │        │         │                                           
 │   │        │         │                                           
 │   │        │         │                                           
 │   │        │         │                                           
 │   │        │         └── Six-character checksum                  
 │   │        └──────────── Data part                               
 │   └───────────────────── Separator between the human-readable    
 │                          part (HRP) and the data part            
 └───────────────────────── Human-readable part (HRP)               
{{</ highlight >}}

Bech32 is defined by [BIP 173](https://github.com/bitcoin/bips/blob/master/bip-0173.mediawiki).

The key piece we care about from BIP 173 is this table, which defines
how to decode the data part.

|       |0|1|2|3|4|5|6|7|
|-------|-|-|-|-|-|-|-|-|
|**+0** |q|p|z|r|y|9|x|8|
|**+8** |g|f|2|t|v|d|w|0|
|**+16**|s|3|j|n|5|4|k|h|
|**+24**|c|e|6|m|u|a|7|l|
