---
title: "Bech32 encoding"
date: 2022-08-08T17:59:31+10:00
draft: true
tags: []
---

* What is an LNURL?
* What is Bech32?
* How do you decode an LNURL?

{{< highlight html >}}
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
