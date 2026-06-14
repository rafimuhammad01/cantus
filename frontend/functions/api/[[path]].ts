// Proxies every /api/* request from the Cloudflare Pages domain to the EC2 backend.
//
// Browser → https://<cantus>.pages.dev/api/... → this Function → http://<EC2_IP>/api/...
//
// Set CANTUS_BACKEND_URL in the Pages project's environment variables, e.g.
// "http://54.169.205.65". Do not include a trailing slash.

export const onRequest: PagesFunction<{ CANTUS_BACKEND_URL: string }> = async (
  context,
) => {
  const { request, env } = context;

  // Debug: surface configuration problems instead of silently throwing.
  const backendURL = env.CANTUS_BACKEND_URL;
  if (!backendURL) {
    return new Response(
      JSON.stringify({
        error: "CANTUS_BACKEND_URL env var missing on Pages",
        env_keys: Object.keys(env),
      }),
      { status: 500, headers: { "content-type": "application/json" } },
    );
  }

  let target: URL;
  try {
    const incoming = new URL(request.url);
    target = new URL(incoming.pathname + incoming.search, backendURL);
  } catch (err) {
    return new Response(
      JSON.stringify({
        error: "failed to build upstream URL",
        backendURL,
        detail: String(err),
      }),
      { status: 500, headers: { "content-type": "application/json" } },
    );
  }

  try {
    const upstream = await fetch(target.toString(), {
      method: request.method,
      headers: request.headers,
      body:
        request.method === "GET" || request.method === "HEAD"
          ? undefined
          : request.body,
      redirect: "manual",
    });
    return new Response(upstream.body, {
      status: upstream.status,
      statusText: upstream.statusText,
      headers: upstream.headers,
    });
  } catch (err) {
    return new Response(
      JSON.stringify({
        error: "upstream fetch failed",
        target: target.toString(),
        detail: String(err),
      }),
      { status: 502, headers: { "content-type": "application/json" } },
    );
  }
};
