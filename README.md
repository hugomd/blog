# Hugo's Blog
This is the repository that houses my blog and personal website. It runs [Hugo](https://gohugo.io/) (I know, I know, it has a very cool name).

It uses the [Hello Friend](https://github.com/panr/hugo-theme-hello-friend) theme and is hosted on [Vercel](https://vercel.app).

## Running locally
```
git submodule update --init --recursive
hugo serve --baseUrl=/ --appendPort=false
```

### Static Assets
Hosted assets are stored in [`/content`](./content), and get served on `/`, e.g. `/about.json`.