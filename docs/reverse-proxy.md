# Reverse proxy

Set `BASE_URL` to the public URL and `SECURE_COOKIES=true` behind HTTPS.

Caddy:

```
movement.example.com {
	request_body {
		max_size 4GB
	}
	reverse_proxy 127.0.0.1:8080 {
		flush_interval -1
	}
}
```

nginx:

```
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_buffering off;            # live updates (/api/v1/live) are server-sent events
    client_max_body_size 4g;        # Google Takeout archives are big
}
```

Phones only need the tracker endpoints reachable (`/api/v1/owntracks/points`, `/api/v1/overland/batches`, `/api/v1/points`, `/api/v1/locations`, `/osmand`, `/gpslogger`, plus `/api/v1/health` and `/api/v1/users/me` for the Dawarich app). Behind a tailnet, keep the phone on the tailnet; tracker apps queue points while offline and send them later.

For `AUTH_MODE=proxy`, have the proxy set `Remote-User` / `Remote-Email` after its own login; device keys keep working for the apps.
