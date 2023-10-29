#!/bin/sh
go install git.sr.ht/~sircmpwn/openring@latest

$GOPATH/bin/openring \
  -n 5 \
  -p 1 \
  -l 0 \
  -s https://kevq.uk/feed/ \
  -s https://joelchrono.xyz/feed.xml \
  -s https://mxb.dev/feed.xml \
  -s https://ndanes.com/feed.xml \
  -s https://renlord.com/index.xml \
  -s https://rusingh.com/feed/ \
  -s https://stanislas.blog/atom.xml \
  -s https://icyphox.sh/blog/feed.xml \
  -s https://nicholas.cloud/rss/ \
  -s https://stefanzweifel.io/rss.xml \
  -s https://healeycodes.com/feed.xml \
  -s https://peppe.rs/index.xml \
  -s http://millicent.gay/index.xml \
  < openring_in.html \
  > ./layouts/partials/openring_out.html

npm run tinacms build && hugo --minify --baseURL=$BASE_URL && rm public/admin/.gitignore
