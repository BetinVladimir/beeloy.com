import { articlePage } from "@/lib/articlePage";

const page = articlePage("articles");
export const generateMetadata = page.generateMetadata;
export default page.Page;
