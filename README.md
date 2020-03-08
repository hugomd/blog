# Hugo's Blog
This is the repository that houses my blog and personal website. It runs [Hugo](https://gohugo.io/) (I know, I know, it has a very cool name).

It uses the [Hello Friend](https://github.com/panr/hugo-theme-hello-friend) theme, is built in Docker, hosted behind nginx, and runs on Kubernetes.

## Running locally
```
hugo server
```

## Building
```
docker build .
```

### Static Assets
Hosted assets are stored in [`/content`](./content), and get served on `/`, e.g. `/about.json`.
