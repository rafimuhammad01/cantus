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
  if (!env.CANTUS_BACKEND_URL) {
    return new Response("CANTUS_BACKEND_URL not configured", { status: 500 });
  }

  const incoming = new URL(request.url);
  const target = new URL(
    incoming.pathname + incoming.search,
    env.CANTUS_BACKEND_URL,
  );

  // Forward method, headers, body. SSE streams forward as-is because Workers'
  // fetch returns a streaming Response body that we pass straight through.
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
};
