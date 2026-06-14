// Minimal Pages Function for diagnosing whether Pages invokes functions at all.
// Will be restored to the proxy version once we confirm it runs.

export const onRequest: PagesFunction<{ CANTUS_BACKEND_URL?: string }> = async (
  context,
) => {
  return new Response(
    JSON.stringify({
      ok: true,
      method: context.request.method,
      url: context.request.url,
      backend_url_set: Boolean(context.env.CANTUS_BACKEND_URL),
      backend_url_value: context.env.CANTUS_BACKEND_URL ?? null,
    }),
    {
      status: 200,
      headers: { "content-type": "application/json" },
    },
  );
};
