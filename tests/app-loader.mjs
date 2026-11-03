export async function resolve(specifier, context, next) {
  if (["gpui-kit", "gpui-base", "herdr"].includes(specifier)) return { url: new URL("./app-host.mjs", import.meta.url).href, shortCircuit: true };
  return next(specifier, context);
}
