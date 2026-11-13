# Frontend image: Vite production build served by nginx, which also
# proxies /api/v1 to the backend service so the app is same-origin.
#
# VITE_API_BASE_URL and VITE_APP_NAME are build-time values inlined by Vite
# into the JavaScript bundle; neither is a secret. The relative
# VITE_API_BASE_URL default keeps every API call on the nginx origin. The
# empty VITE_APP_NAME default keeps the built-in name (GoCRM) on the signed-out
# pages; pass --build-arg VITE_APP_NAME=... to brand them.

FROM node:22-alpine AS build

WORKDIR /app

COPY package.json package-lock.json ./
RUN npm ci

COPY . .

ARG VITE_API_BASE_URL=/api/v1
ENV VITE_API_BASE_URL=$VITE_API_BASE_URL
ARG VITE_APP_NAME=
ENV VITE_APP_NAME=$VITE_APP_NAME

RUN npm run build

FROM nginx:1.27-alpine

COPY docker/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html

EXPOSE 80
