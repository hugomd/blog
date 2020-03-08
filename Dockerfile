FROM hugomd/hugo-base-image:latest

WORKDIR /src
COPY . /src
RUN hugo -t hello-friend --baseUrl=https://hugo.md

FROM nginx:1.15.6-alpine
COPY ./nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=0 /src/public /usr/share/nginx/html

EXPOSE 80
