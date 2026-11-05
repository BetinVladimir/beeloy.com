import { articlePage } from "@/lib/articlePage";

const page = articlePage("news");
export const generateMetadata = page.generateMetadata;
export default page.Page;
