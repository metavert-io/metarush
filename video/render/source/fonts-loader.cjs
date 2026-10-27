// A webpack loader MetaRush adds when it bundles a Remotion source (D134):
// @remotion/google-fonts fetches font files from fonts.gstatic.com while the
// video renders, which the render jail refuses. MetaRush downloads the files
// once when the project is imported and puts them in the bundle, and this
// loader points the module at those copies (same origin, loopback).
module.exports = function metarushGoogleFonts(source) {
  return String(source).split('https://fonts.gstatic.com/').join('/__metarush_fonts/');
};
