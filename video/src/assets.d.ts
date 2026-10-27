// Font files are imported as URLs (webpack asset/resource in Remotion, Vite assets in the dashboard).
declare module '*.woff2' {
  const url: string;
  export default url;
}
