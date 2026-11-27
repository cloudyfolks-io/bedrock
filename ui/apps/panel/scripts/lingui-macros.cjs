const { transformSync } = require("@babel/core");

module.exports = function transformLingui(source, inputMap) {
  const result = transformSync(source, {
    filename: this.resourcePath,
    babelrc: false,
    configFile: false,
    parserOpts: { plugins: ["jsx", "typescript"] },
    plugins: ["@lingui/babel-plugin-lingui-macro"],
    inputSourceMap: inputMap,
    sourceMaps: true,
  });
  this.callback(null, result.code, result.map);
};
