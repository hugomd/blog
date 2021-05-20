'use strict';

const Hapi = require('@hapi/hapi');
const axios = require('axios');

const delay = (duration) => new Promise(resolve => setTimeout(() => resolve(true), duration));

const instance = axios.create({
  headers: {
    'Authorization': `Bearer ${process.env.TWITTER_BEARER_TOKEN}`
  }
});

const init = async () => {

  const server = Hapi.server({
    port: 3000,
    host: 'localhost'
  });

  server.route({
    method: 'GET',
    path: '/',
    handler: async (request, h) => {
      const { auth, tweetId } = request.query;
      if (auth !== process.env.AUTH) return h.response().code(401);
      if (!tweetId) return h.response().code(400);

      return instance.get(`https://api.twitter.com/1.1/statuses/show?tweet_mode=extended&id=${tweetId}`).then(({data}) => data);
    }
  });

  await server.start();
  console.log('Server running on %s', server.info.uri);

  await delay(20000);

  await server.stop();
};

process.on('unhandledRejection', (err) => {

  console.log(err);
  process.exit(1);
});

init();
