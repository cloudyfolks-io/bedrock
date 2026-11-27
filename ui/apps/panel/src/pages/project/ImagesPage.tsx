import { useState } from "react";
import { msg } from "@lingui/core/macro";
import { useLingui } from "@lingui/react/macro";
import { Tabs as BaseTabs } from "@base-ui/react/tabs";
import { Tab, TabPanel, Tabs } from "@bedrock/design";
import type { KubeObject } from "../../api/types";
import { useCans } from "../../authz/authz";
import { PageHeader } from "../../components/PageHeader";
import { definitionFor } from "../../resources/registry";
import { ResourceListPage } from "../../resources/ResourceListPage";
import { useCurrentProject } from "../../shell/context";
import { ImageUpload } from "./ImageUpload";
import styles from "./storage.module.css";

export default function ImagesPage() {
  const { t } = useLingui();
  const images = definitionFor("images");
  const catalog = definitionFor("clusterimages");
  const project = useCurrentProject();
  const [tab, setTab] = useState("project");
  const [uploading, setUploading] = useState<KubeObject | undefined>();
  const [canUpload] = useCans(project ? [{ verb: "create", group: "bedrock.cloudyfolks.io", resource: "images", subresource: "upload", namespace: project.namespace }] : []);
  if (!images || !catalog) {
    return null;
  }
  return (
    <div className={styles.imageTabs}>
      <PageHeader title={t({ id: "nav.item.images", message: "Images" })} />
      <Tabs value={tab} onValueChange={setTab}>
        <BaseTabs.List className="br-tab-list" aria-label={t({ id: "nav.item.images", message: "Images" })}>
          <Tab value="project">{t({ id: "images.tab.project", message: "Project images" })}</Tab>
          <Tab value="catalog">{t({ id: "images.tab.catalog", message: "Catalog" })}</Tab>
        </BaseTabs.List>
        <TabPanel value="project">
          <ResourceListPage
            def={{ ...images, title: msg({ id: "images.tab.project", message: "Project images" }) }}
            embedded
            hideEmbeddedTitle
            rowItems={(object) =>
              canUpload === "allowed" && object.status?.phase === "WaitingForUpload"
                ? [{ id: "upload", label: t({ id: "images.upload.start", message: "Upload" }), onSelect: () => setUploading(object) }]
                : []
            }
          >
            {uploading ? <ImageUpload image={uploading} onClose={() => setUploading(undefined)} /> : null}
          </ResourceListPage>
        </TabPanel>
        <TabPanel value="catalog">
          <ResourceListPage def={{ ...catalog, title: msg({ id: "images.tab.catalog", message: "Catalog" }) }} embedded hideEmbeddedTitle readOnly />
        </TabPanel>
      </Tabs>
    </div>
  );
}
