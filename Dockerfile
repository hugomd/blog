FROM reg.g.hu.md/hugo/now-hugo-image:latest

WORKDIR /src
COPY . /src
RUN hugo -t hugo-cactus-theme --baseUrl=https://hugo.md

FROM nginx:1.15.6-alpine
COPY ./default.conf /etc/nginx/conf.d/default.conf
COPY --from=0 /src/public /usr/share/nginx/html
COPY ./about.json /usr/share/nginx/html
