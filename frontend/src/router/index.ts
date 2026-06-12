import { createRouter, createWebHistory } from "vue-router";
import SearchView from "../views/SearchView.vue";

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: "/",
      component: SearchView,
    },
    {
      path: "/preview/:videoId",
      component: () => import("@/views/PreviewView.vue"),
    },
    {
      path: "/play/:videoId/:semitones",
      component: () => import("@/views/PlayView.vue"),
    },
    {
      path: "/:pathMatch(.*)*",
      redirect: "/",
    },
  ],
});

export default router;
