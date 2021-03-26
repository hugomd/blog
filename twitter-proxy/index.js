'use strict';

const Hapi = require('@hapi/hapi');
const axios = require('axios');

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

      return instance.get(`https://api.twitter.com/1.1/statuses/show?id=${tweetId}`).then(({data}) => data)
    }
  });

  await server.start();
  console.log('Server running on %s', server.info.uri);
};

process.on('unhandledRejection', (err) => {

  console.log(err);
  process.exit(1);
});

init();
