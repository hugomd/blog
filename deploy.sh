#!/bin/sh
ssh root@128.199.232.205 << EOF
	cd /var/www/hugo.sx/blog/
	git pull
	exit
EOF
