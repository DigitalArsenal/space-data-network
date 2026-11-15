// Link only with FlatSQL 3.7.2 (1b3ae18c00bb7a8917acc3db32b903d6ae8affea)
// and that commit's p4_test_main.cpp. Synthetic data; no signatures are made.
#include "p4/p4_test.h"
#include <flatbuffers/reflection.h>
#include <filesystem>
#include <fstream>
#include <iterator>
#include <cstring>
#include <cstdlib>
using namespace p4t;
static bool putChecked(const Batch& batch) {
    const auto result=put(batch);
    if(result.status!=P4_OK || result.rows.size()!=batch.recs.size()) return false;
    for(size_t i=0;i<result.rows.size();i++) if(result.i(i,"reject")) {
        std::fprintf(stderr,"fixture %s row %zu rejected: %lld\n",batch.type.c_str(),i,(long long)result.i(i,"reject"));
        return false;
    }
    return true;
}

static void capture(std::ofstream& out,const std::string& name,const Result& result,bool comma=true) {
    if(result.status!=P4_OK) { std::fprintf(stderr,"capture %s failed: %s\n",name.c_str(),result.err.c_str()); std::abort(); }
    if(comma) out<<",";
    out<<"\""<<name<<"\":{\"columns\":[";
    for(size_t i=0;i<result.cols.size();i++) { if(i) out<<","; out<<"\""<<result.cols[i]<<"\""; }
    out<<"],\"rows\":[";
    for(size_t i=0;i<result.rows.size();i++) {
        if(i) out<<","; out<<"[";
        for(size_t j=0;j<result.rows[i].size();j++) {
            if(j) out<<",";
            const auto& cell=result.rows[i][j]; out<<"{\"type\":"<<int(cell.type);
            if(cell.type==1) out<<",\"i\":"<<cell.i;
            else if(cell.type==2) out<<",\"d\":"<<cell.d;
            else if(cell.type==3 || cell.type==4) out<<",\"hex\":\""<<hex(reinterpret_cast<const uint8_t*>(cell.s.data()),cell.s.size())<<"\"";
            out<<"}";
        }
        out<<"]";
    }
    out<<"]}";
}

static TestType realSchema(TestType type,const std::string& dir) {
    std::ifstream in(dir+"/"+type.name+".bfbs",std::ios::binary);
    type.bfbs.assign(std::istreambuf_iterator<char>(in),{});
    if(type.bfbs.empty()) { std::fprintf(stderr,"missing schema for %s\n",type.name.c_str()); std::abort(); }
    type.schema=reflection::GetSchema(type.bfbs.data());
    return type;
}

P4_TEST(generate_format4_fixture) {
    const std::string root=argStr("store",""),schemas=argStr("bfbs","");
    REQUIRE(!root.empty() && !schemas.empty(),"explicit output and schema paths required");
    REQUIRE(!std::filesystem::exists(root),"refuse an existing output");
    EngineOpts opts; opts.createMode=2; opts.gseqFloor=100;
    REQUIRE(openEngine(root+"/fsql4",opts)==P4_OK,"old engine creates the fixture");
    auto omm=realSchema(ommType(),schemas),iqc=realSchema(iqcType(),schemas);
    REQUIRE(registerType(omm)==P4_OK && registerType(iqc)==P4_OK,"register canonical schemas");
    Batch b; b.type="OMM"; b.peer="source:fixture"; b.mode=1; b.at=1700000010;
    b.tags={{"fixture-provider","fixture-source","https://fixture.invalid/one","batch-a","public","fixture-peer","fixture-public"},
            {"fixture-provider","fixture-source","https://fixture.invalid/two","batch-b","public","fixture-peer","fixture-public"}};
    for(int i=0;i<3;i++) {
        In r; r.frame=buildFrame(omm,{Field::u64("NORAD_CAT_ID",100+i),Field::str("OBJECT_ID","fixture-"+std::to_string(i)),
            Field::str("OBJECT_NAME","synthetic fixture"),Field::str("EPOCH","2026-09-01T00:00:00Z"),
            Field::str("CREATION_DATE","2026-09-01T00:00:00Z"),Field::f64("MEAN_MOTION",1.0)});
        r.seq=100+i; r.ts=1700000000+i; r.sig={0xde,0xad,uint8_t(i)};
        r.tags={{0,1700000010+i},{1,1700000020+i}};
        if(i==2) r.sealed={'S','D','F','1',1,0x11,0x22,0x33,0x44};
        b.recs.push_back(std::move(r));
    }
    REQUIRE(putChecked(b),"migrate rows with provenance and a sealed payload");
    Batch second=b; second.peer="source:second"; second.tags.clear();
    second.recs.resize(1); second.recs[0].tags.clear();
    REQUIRE(putChecked(second),"a second producer copy");
    Batch capture; capture.type="IQC"; capture.peer="source:capture"; capture.mode=1; capture.at=1700000030;
    capture.tags={{"fixture-provider","capture","https://fixture.invalid/capture","capture-batch","public","fixture-peer","fixture-public"}};
    In record; record.frame=buildFrame(iqc,{Field::str("ID","fixture-capture"),Field::str("CAPTURE_ID","capture-1"),
        Field::str("DESCRIPTION","synthetic fixture"),Field::f64("SAMPLE_RATE_HZ",1.0)});
    record.seq=211; record.ts=1700000030; record.tags={{0,1700000031}}; record.hasIdent=true;
    std::memset(record.ident,0x5a,sizeof record.ident); capture.recs.push_back(record);
    REQUIRE(putChecked(capture),"an ingest identity");
    TlvW rebuild; rebuild.u32(63,1);
    REQUIRE(call(P4_OPC_REBUILD,rebuild.b).status==P4_OK,"old engine builds the indexes");
    REQUIRE(flatsql_p4_activate()==P4_OK,"old engine activates format 4");
    REQUIRE(closeEngine()==P4_OK,"old engine closes the fixture");
    opts.createMode=0;
    REQUIRE(openEngine(root+"/fsql4",opts)==P4_OK,"old engine reopens its durable fixture");
    std::ofstream out(root+"/expected-raw.json"); out<<"{";
    for(const auto& batch:{b,capture}) {
        std::vector<std::vector<uint8_t>> cids;
        for(const auto& row:batch.recs) { uint8_t cid[36]; cidOf(row.frame,cid); cids.emplace_back(cid,cid+36); }
        ::capture(out,"records_"+batch.type,get(batch.type,cids,true,true),batch.type!="OMM");
        TlvW head; head.text(1,batch.type); ::capture(out,"head_"+batch.type,call(P4_OPC_HEAD,head.b));
        TlvW tags; tags.text(1,batch.type); std::vector<uint8_t> ids(4);
        const uint32_t count=uint32_t(cids.size());
        for(int i=0;i<4;i++) ids[size_t(i)]=uint8_t(count>>(8*i));
        for(const auto& id:cids) ids.insert(ids.end(),id.begin(),id.end());
        tags.raw(40,ids.data(),ids.size()); ::capture(out,"tags_"+batch.type,call(P4_OPC_TAGS,tags.b));
    }
    for(const auto& query:{std::make_pair("types",1),std::make_pair("partitions",2),std::make_pair("lanes",3)}) {
        TlvW request; request.u8(45,uint8_t(query.second)); ::capture(out,query.first,call(P4_OPC_SUMMARY,request.b));
    }
    out<<"}\n"; out.close();
    REQUIRE(closeEngine()==P4_OK,"old engine closes after capturing every public field");
}
