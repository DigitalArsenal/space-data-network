import path from "node:path";
import { fileURLToPath } from "node:url";
import { mkdir, readFile, writeFile } from "node:fs/promises";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const packageRoot = path.resolve(__dirname, "..");
const repoRoot = path.resolve(packageRoot, "..", "..");
const distDir = path.join(packageRoot, "dist");

const templatePath = path.join(packageRoot, "src", "index.template.html");
const appPath = path.join(packageRoot, "src", "app.js");
const orbProPath =
  process.env.ORBPRO_ESM_PATH ||
  path.resolve(repoRoot, "..", "OrbPro", "Build", "OrbPro", "OrbPro.esm.js");

async function build() {
  const [template, appModule, orbProModule] = await Promise.all([
    readFile(templatePath, "utf8"),
    readFile(appPath, "utf8"),
    readFile(orbProPath, "utf8"),
  ]);

  const buildStamp = new Date().toISOString();

  const appWithOrbPro = appModule.replace(
    "__ORBPRO_ESM_SOURCE__",
    JSON.stringify(orbProModule),
  );

  const html = template
    .replace("__APP_MODULE__", appWithOrbPro)
    .replace("__BUILD_STAMP__", buildStamp);

  await mkdir(distDir, { recursive: true });
  const indexPath = path.join(distDir, "index.html");
  const singleFilePath = path.join(distDir, "spaceaware.single.html");
  await Promise.all([
    writeFile(indexPath, html, "utf8"),
    writeFile(singleFilePath, html, "utf8"),
  ]);

  console.log("Built SpaceAware single-file app:");
  console.log(`- ${indexPath}`);
  console.log(`- ${singleFilePath}`);
  console.log(`- Embedded OrbPro source: ${orbProPath}`);
}

build().catch((err) => {
  console.error("SpaceAware build failed:");
  console.error(err);
  process.exit(1);
});
