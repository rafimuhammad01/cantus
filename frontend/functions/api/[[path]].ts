// Proxies /api/* from Cloudflare Pages to the EC2 backend.
//
// Browser → https://<cantus>.pages.dev/api/... → this Function → https://<EC2>/api/...
//
// Set CANTUS_BACKEND_URL in Pages env vars, e.g. "https://54-169-205-65.sslip.io".

export const onRequest: PagesFunction<{ CANTUS_BACKEND_URL: string }> = async (
  context,
) => {
  const { request, env } = context;

  if (!env.CANTUS_BACKEND_URL) {
    return new Response(
      JSON.stringify({ error: "CANTUS_BACKEND_URL not set" }),
      { status: 500, headers: { "content-type": "application/json" } },
    );
  }

  const incoming = new URL(request.url);
  const target =
    env.CANTUS_BACKEND_URL.replace(/\/$/, "") +
    incoming.pathname +
    incoming.search;

  try {
    const init: RequestInit = {
      method: request.method,
      headers: request.headers,
      redirect: "manual",
    };
    if (request.method !== "GET" && request.method !== "HEAD") {
      init.body = request.body;
    }
    const upstream = await fetch(target, init);
    return new Response(upstream.body, {
      status: upstream.status,
      statusText: upstream.statusText,
      headers: upstream.headers,
    });
  } catch (err) {
    return new Response(
      JSON.stringify({
        error: "upstream fetch failed",
        target,
        detail: String(err),
      }),
      { status: 502, headers: { "content-type": "application/json" } },
    );
  }
};
