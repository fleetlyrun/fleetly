function r(t,e){const n=new Map;for(const[c,p]of t)for(const o of e(p)){const s=n.get(o)??[];s.push(c),n.set(o,s)}return n}export{r as s};
